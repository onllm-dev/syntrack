"""E2E tests for responsive design.

3 tests covering mobile, tablet, and desktop viewport sizes.
"""
import pytest
from playwright.sync_api import Page, expect

BASE_URL = "http://localhost:19211"
USERNAME = "admin"
PASSWORD = "testpass123"


def _login(page: Page) -> None:
    """Helper to log in on the given page."""
    page.goto(f"{BASE_URL}/login")
    page.fill("#username", USERNAME)
    page.fill("#password", PASSWORD)
    page.click("button.login-button")
    page.wait_for_url(f"{BASE_URL}/", timeout=10000)


class TestResponsive:
    """Responsive layout tests at different viewport sizes."""

    def test_mobile_375x667(self, page: Page) -> None:
        """Dashboard should be usable on a mobile viewport (375x667)."""
        page.set_viewport_size({"width": 375, "height": 667})
        _login(page)

        # Core elements should be visible
        expect(page.locator(".app-header")).to_be_visible()
        expect(page.locator(".main-content")).to_be_visible()

        # Switch to Synthetic to verify static cards render on mobile
        tabs = page.query_selector_all(".provider-tab")
        for tab in tabs:
            if "Synthetic" in (tab.inner_text() or ""):
                tab.click()
                page.wait_for_load_state("networkidle", timeout=10000)
                break

        page.wait_for_timeout(1000)
        cards = page.query_selector_all("article.quota-card")
        visible_cards = [c for c in cards if c.is_visible()]
        # On mobile, cards should be visible (stacked vertically)
        assert len(visible_cards) >= 1

    def test_tablet_768x1024(self, page: Page) -> None:
        """Dashboard should render properly on a tablet viewport (768x1024)."""
        page.set_viewport_size({"width": 768, "height": 1024})
        _login(page)

        expect(page.locator(".app-header")).to_be_visible()
        expect(page.locator(".main-content")).to_be_visible()

        # Provider tabs should be visible
        page.wait_for_timeout(2000)
        tabs = page.query_selector_all(".provider-tab")
        if len(tabs) > 0:
            assert any(tab.is_visible() for tab in tabs)

    def test_desktop_1920x1080(self, page: Page) -> None:
        """Dashboard should render fully on a desktop viewport (1920x1080)."""
        page.set_viewport_size({"width": 1920, "height": 1080})
        _login(page)

        expect(page.locator(".app-header")).to_be_visible()
        expect(page.locator(".main-content")).to_be_visible()

        # Provider tabs should be visible and not collapsed
        page.wait_for_timeout(2000)
        tabs = page.query_selector_all(".provider-tab")
        if len(tabs) > 0:
            for tab in tabs:
                assert tab.is_visible()

        # Footer should be visible
        expect(page.locator(".app-footer")).to_be_visible()
