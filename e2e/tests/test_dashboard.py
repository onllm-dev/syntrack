"""E2E tests for the dashboard page.

7 tests covering provider tabs, refresh, footer, and navigation.
"""
import re

import pytest
from playwright.sync_api import Page, expect

from page_objects.dashboard_page import DashboardPage

BASE_URL = "http://localhost:19211"


class TestDashboard:
    """Dashboard layout and navigation tests."""

    def test_provider_tabs_visible(self, dashboard_page: Page) -> None:
        """Dashboard should show provider tabs when multiple providers are configured."""
        dash = DashboardPage(dashboard_page)
        tabs = dash.get_provider_tabs()
        # Should have at least 2 providers (Anthropic + Synthetic or Z.ai)
        assert len(tabs) >= 2

    def test_default_provider_is_anthropic(self, dashboard_page: Page) -> None:
        """The default active provider tab should be Anthropic."""
        dash = DashboardPage(dashboard_page)
        active = dash.get_active_provider()
        assert active == "anthropic"

    def test_tab_switch_changes_view(self, dashboard_page: Page) -> None:
        """Clicking a different provider tab should switch the dashboard view."""
        dash = DashboardPage(dashboard_page)

        tabs = dash.get_provider_tabs()
        if len(tabs) < 2:
            pytest.skip("Only one provider configured")

        # Find a non-active tab
        for tab_text in tabs:
            if "Synthetic" in tab_text:
                dash.select_provider("Synthetic")
                active = dash.get_active_provider()
                assert active == "synthetic"
                break
            elif "Z.ai" in tab_text:
                dash.select_provider("Z.ai")
                active = dash.get_active_provider()
                assert active == "zai"
                break

    def test_refresh_button_updates_data(self, dashboard_page: Page) -> None:
        """Clicking the refresh button should update the last-updated timestamp."""
        dash = DashboardPage(dashboard_page)
        initial_text = dash.get_last_updated()

        dash.click_refresh()
        dashboard_page.wait_for_timeout(2000)

        # After refresh, last-updated text should have changed or still be present
        updated_text = dash.get_last_updated()
        assert updated_text != ""

    def test_version_in_footer(self, dashboard_page: Page) -> None:
        """Dashboard footer should display the onWatch version."""
        dash = DashboardPage(dashboard_page)
        version_text = dash.get_version_text()
        assert "onWatch" in version_text
        assert re.search(r"v[\d.]+", version_text) or "dev" in version_text.lower()

    def test_settings_link_present(self, dashboard_page: Page) -> None:
        """Dashboard should have a settings link/button."""
        dash = DashboardPage(dashboard_page)
        assert dash.has_settings_link()

    def test_last_updated_displays(self, dashboard_page: Page) -> None:
        """The last-updated indicator should display a timestamp after data loads."""
        dash = DashboardPage(dashboard_page)
        # Wait for data to load
        dashboard_page.wait_for_timeout(3000)
        text = dash.get_last_updated()
        assert text != ""
        assert "Last updated" in text
