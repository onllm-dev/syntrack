"""Pytest configuration and fixtures for onWatch E2E tests.

Session-scoped fixtures build and start the mock server and onwatch binary,
then tear them down after all tests complete.
"""
import os
import signal
import subprocess
import time
from pathlib import Path
from typing import Generator

import pytest
import urllib.request
import urllib.error

# Ports
ONWATCH_PORT = 19211
MOCK_PORT = 19212
BASE_URL = f"http://localhost:{ONWATCH_PORT}"
MOCK_URL = f"http://localhost:{MOCK_PORT}"

# Credentials
USERNAME = "admin"
PASSWORD = "testpass123"

# Paths
PROJECT_ROOT = Path(__file__).resolve().parent.parent
MOCK_BINARY = "/tmp/mockserver-test"
ONWATCH_BINARY = "/tmp/onwatch-test"
# E2E isolation: override HOME so the canonical DB path (~/.onwatch/data/onwatch.db)
# does not exist. This prevents main.go's fixExplicitDBPath() from redirecting to
# the production database.
E2E_HOME = "/tmp/onwatch-e2e-home"
DB_PATH = "/tmp/onwatch-e2e.db"


def _wait_for_http(url: str, timeout: float = 30.0, interval: float = 0.5) -> bool:
    """Poll an HTTP URL until it returns 200 or timeout is reached."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            req = urllib.request.Request(url, method="GET")
            resp = urllib.request.urlopen(req, timeout=5)
            if resp.status == 200:
                return True
        except (urllib.error.URLError, OSError, ConnectionRefusedError):
            pass
        time.sleep(interval)
    return False


def _kill_process(proc: subprocess.Popen) -> None:
    """Kill a subprocess and wait for it to exit."""
    if proc.poll() is None:
        try:
            proc.send_signal(signal.SIGTERM)
            proc.wait(timeout=5)
        except (subprocess.TimeoutExpired, OSError):
            proc.kill()
            proc.wait(timeout=5)


@pytest.fixture(scope="session")
def mock_server() -> Generator[subprocess.Popen, None, None]:
    """Build and start the mock server binary."""
    # Build mock server
    result = subprocess.run(
        ["go", "build", "-o", MOCK_BINARY, "./internal/testutil/cmd/mockserver"],
        cwd=str(PROJECT_ROOT),
        capture_output=True,
        text=True,
        timeout=120,
    )
    assert result.returncode == 0, f"Mock server build failed: {result.stderr}"

    # Start mock server
    proc = subprocess.Popen(
        [
            MOCK_BINARY,
            f"--port={MOCK_PORT}",
            "--syn-key=syn_test_e2e_key",
            "--zai-key=zai_test_e2e_key",
            "--anth-token=anth_test_e2e_token",
        ],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )

    # Wait for mock server to be ready
    ready = _wait_for_http(f"{MOCK_URL}/admin/requests", timeout=15)
    assert ready, "Mock server did not start in time"

    yield proc

    _kill_process(proc)
    # Clean up binary
    try:
        os.unlink(MOCK_BINARY)
    except OSError:
        pass


@pytest.fixture(scope="session")
def onwatch_server(mock_server: subprocess.Popen) -> Generator[subprocess.Popen, None, None]:
    """Build and start the onwatch binary."""
    # Clean up any stale DB and home directory
    import shutil
    for path in [DB_PATH, f"{DB_PATH}-journal", f"{DB_PATH}-wal", f"{DB_PATH}-shm"]:
        try:
            os.unlink(path)
        except OSError:
            pass
    if os.path.exists(E2E_HOME):
        shutil.rmtree(E2E_HOME)
    os.makedirs(E2E_HOME, exist_ok=True)

    # Build onwatch
    result = subprocess.run(
        ["go", "build", "-o", ONWATCH_BINARY, "."],
        cwd=str(PROJECT_ROOT),
        capture_output=True,
        text=True,
        timeout=120,
    )
    assert result.returncode == 0, f"onWatch build failed: {result.stderr}"

    env = os.environ.copy()
    env.update({
        "HOME": E2E_HOME,
        "ONWATCH_ADMIN_PASS": PASSWORD,
        "SYNTHETIC_API_KEY": "syn_test_e2e_key",
        "ZAI_API_KEY": "zai_test_e2e_key",
        "ZAI_BASE_URL": f"http://localhost:{MOCK_PORT}",
        "ANTHROPIC_TOKEN": "anth_test_e2e_token",
    })

    proc = subprocess.Popen(
        [
            ONWATCH_BINARY,
            "--debug",
            f"--port={ONWATCH_PORT}",
            "--interval=10",
            "--test",
            f"--db={DB_PATH}",
        ],
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )

    # Wait for onwatch to be ready (login page returns 200)
    ready = _wait_for_http(f"{BASE_URL}/login", timeout=30)
    assert ready, "onWatch server did not start in time"

    yield proc

    _kill_process(proc)
    # Clean up
    try:
        os.unlink(ONWATCH_BINARY)
    except OSError:
        pass
    for path in [DB_PATH, f"{DB_PATH}-journal", f"{DB_PATH}-wal", f"{DB_PATH}-shm"]:
        try:
            os.unlink(path)
        except OSError:
            pass
    import shutil
    if os.path.exists(E2E_HOME):
        shutil.rmtree(E2E_HOME, ignore_errors=True)


@pytest.fixture(autouse=True, scope="session")
def servers(mock_server: subprocess.Popen, onwatch_server: subprocess.Popen) -> Generator[None, None, None]:
    """Ensure both servers are running for all tests."""
    yield


@pytest.fixture
def authenticated_page(page):
    """Log in and return a page with a valid session cookie."""
    page.goto(f"{BASE_URL}/login")
    page.fill("#username", USERNAME)
    page.fill("#password", PASSWORD)
    page.click("button.login-button")
    # Wait for redirect to dashboard
    page.wait_for_url(f"{BASE_URL}/", timeout=10000)
    return page


@pytest.fixture
def dashboard_page(authenticated_page):
    """Return an authenticated page on the dashboard."""
    # Already on dashboard after login
    authenticated_page.wait_for_selector(".app-header", timeout=10000)
    return authenticated_page


@pytest.fixture
def settings_page(authenticated_page):
    """Navigate to the settings page and return the page."""
    authenticated_page.goto(f"{BASE_URL}/settings")
    authenticated_page.wait_for_selector(".settings-page", timeout=10000)
    return authenticated_page
