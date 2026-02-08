"""E2E tests for authentication flow.

8 tests covering login, logout, session persistence, and API auth.
"""
import re

import pytest
from playwright.sync_api import Page, expect

from page_objects.login_page import LoginPage

BASE_URL = "http://localhost:19211"
USERNAME = "admin"
PASSWORD = "testpass123"


class TestAuth:
    """Authentication and session management tests."""

    def test_login_page_renders(self, page: Page) -> None:
        """Login page should display the login form with username, password, and submit."""
        login = LoginPage(page)
        login.goto()

        assert login.is_visible()
        expect(page.locator("#username")).to_be_visible()
        expect(page.locator("#password")).to_be_visible()
        expect(page.locator("button.login-button")).to_be_visible()
        assert "Login" in login.get_title()

    def test_login_success_redirects_to_dashboard(self, page: Page) -> None:
        """Successful login should redirect to the dashboard."""
        login = LoginPage(page)
        login.goto()
        login.login(USERNAME, PASSWORD)

        page.wait_for_url(f"{BASE_URL}/", timeout=10000)
        expect(page.locator(".app-header")).to_be_visible()

    def test_login_failure_shows_error(self, page: Page) -> None:
        """Failed login should redirect back to login with an error message."""
        login = LoginPage(page)
        login.goto()
        login.login(USERNAME, "wrongpassword")

        # Should stay on login page with error
        page.wait_for_url(re.compile(r"/login\?error="), timeout=10000)
        assert login.is_visible()
        error_msg = login.get_error_message()
        assert "Invalid" in error_msg or "invalid" in error_msg.lower()

    def test_unauthenticated_redirect_to_login(self, page: Page) -> None:
        """Accessing the dashboard without auth should redirect to /login."""
        # Clear any existing cookies
        page.context.clear_cookies()
        page.goto(f"{BASE_URL}/")
        page.wait_for_url(re.compile(r"/login"), timeout=10000)
        expect(page.locator(".login-card")).to_be_visible()

    def test_logout_redirects_to_login(self, page: Page) -> None:
        """Clicking logout should clear the session and redirect to login."""
        # First login
        login = LoginPage(page)
        login.goto()
        login.login(USERNAME, PASSWORD)
        page.wait_for_url(f"{BASE_URL}/", timeout=10000)

        # Now logout
        page.goto(f"{BASE_URL}/logout")
        page.wait_for_url(re.compile(r"/login"), timeout=10000)
        expect(page.locator(".login-card")).to_be_visible()

        # Verify we can't access dashboard anymore
        page.goto(f"{BASE_URL}/")
        page.wait_for_url(re.compile(r"/login"), timeout=10000)

    def test_session_persists_across_navigation(self, authenticated_page: Page) -> None:
        """An authenticated session should persist across page navigation."""
        authenticated_page.goto(f"{BASE_URL}/settings")
        expect(authenticated_page.locator(".settings-page")).to_be_visible()

        authenticated_page.goto(f"{BASE_URL}/")
        expect(authenticated_page.locator(".app-header")).to_be_visible()

    def test_password_visibility_toggle(self, page: Page) -> None:
        """The password toggle button should switch input type between password and text."""
        login = LoginPage(page)
        login.goto()

        # Initially password type
        assert login.get_password_input_type() == "password"

        login.toggle_password_visibility()
        # After toggle, should be text
        page.wait_for_timeout(300)
        input_type = login.get_password_input_type()
        assert input_type == "text"

        login.toggle_password_visibility()
        page.wait_for_timeout(300)
        assert login.get_password_input_type() == "password"

    def test_api_returns_401_without_auth(self, page: Page) -> None:
        """API endpoints should return 401 JSON without a valid session."""
        page.context.clear_cookies()

        response = page.request.get(f"{BASE_URL}/api/current")
        assert response.status == 401
        data = response.json()
        assert "error" in data
        assert data["error"] == "unauthorized"
