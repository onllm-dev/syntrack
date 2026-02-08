"""E2E tests for chart rendering.

3 tests covering canvas rendering, range buttons, and chart after provider switch.
"""
import pytest
from playwright.sync_api import Page, expect

from page_objects.dashboard_page import DashboardPage

BASE_URL = "http://localhost:19211"


class TestCharts:
    """Chart rendering and interaction tests."""

    def test_chart_canvas_renders(self, dashboard_page: Page) -> None:
        """The usage chart canvas element should be present in the DOM."""
        dash = DashboardPage(dashboard_page)
        dash.scroll_to_section("chart-section")

        expect(dashboard_page.locator("#usage-chart")).to_be_visible()
        assert dash.get_chart_canvas()

    def test_chart_range_buttons(self, dashboard_page: Page) -> None:
        """Chart range buttons should be present and clickable."""
        dash = DashboardPage(dashboard_page)
        dash.scroll_to_section("chart-section")
        dashboard_page.wait_for_timeout(1000)

        # Check all range buttons exist
        for range_val in ["1h", "6h", "24h", "7d", "30d"]:
            expect(
                dashboard_page.locator(f'.range-btn[data-range="{range_val}"]')
            ).to_be_visible()

        # Click a range button and verify it becomes active
        dash.select_chart_range("24h")
        dashboard_page.wait_for_timeout(1000)
        assert dash.get_active_chart_range() == "24h"

        # Switch to another range
        dash.select_chart_range("1h")
        dashboard_page.wait_for_timeout(1000)
        assert dash.get_active_chart_range() == "1h"

    def test_chart_after_provider_switch(self, dashboard_page: Page) -> None:
        """The chart should still render after switching provider tabs."""
        dash = DashboardPage(dashboard_page)
        tabs = dash.get_provider_tabs()

        if len(tabs) < 2:
            pytest.skip("Only one provider configured")

        # Switch to a different provider
        for tab_text in tabs:
            if "Synthetic" in tab_text:
                dash.select_provider("Synthetic")
                break
            elif "Z.ai" in tab_text:
                dash.select_provider("Z.ai")
                break

        dashboard_page.wait_for_timeout(2000)
        dash.scroll_to_section("chart-section")

        expect(dashboard_page.locator("#usage-chart")).to_be_visible()
        assert dash.get_chart_canvas()
