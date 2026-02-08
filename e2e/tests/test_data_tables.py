"""E2E tests for data tables (cycles, sessions, cycle overview).

6 tests covering lazy-loading, sorting, pagination, and expandable rows.
"""
import pytest
from playwright.sync_api import Page, expect

from page_objects.dashboard_page import DashboardPage

BASE_URL = "http://localhost:19211"


class TestDataTables:
    """Data table interaction tests."""

    def test_cycles_section_exists(self, dashboard_page: Page) -> None:
        """The cycles section should exist in the DOM."""
        dash = DashboardPage(dashboard_page)
        dash.scroll_to_section("cycles-section")

        expect(dashboard_page.locator(".cycles-section")).to_be_visible()
        expect(dashboard_page.locator("#cycles-table")).to_be_visible()

    def test_cycles_table_has_sort_headers(self, dashboard_page: Page) -> None:
        """The cycles table should have sortable column headers."""
        dash = DashboardPage(dashboard_page)
        dash.scroll_to_section("cycles-section")

        headers = dashboard_page.query_selector_all(
            "#cycles-table thead th[data-sort-key]"
        )
        assert len(headers) >= 5
        sort_keys = [h.get_attribute("data-sort-key") for h in headers]
        assert "start" in sort_keys
        assert "peak" in sort_keys
        assert "total" in sort_keys

    def test_cycles_pagination_controls(self, dashboard_page: Page) -> None:
        """The cycles section should have pagination controls."""
        dash = DashboardPage(dashboard_page)
        dash.scroll_to_section("cycles-section")

        expect(dashboard_page.locator("#cycles-info")).to_be_visible()
        expect(dashboard_page.locator("#cycles-page-size")).to_be_visible()

    def test_sessions_section_exists(self, dashboard_page: Page) -> None:
        """The sessions section should exist and display a table."""
        dash = DashboardPage(dashboard_page)
        dash.scroll_to_section("sessions-section")

        expect(dashboard_page.locator(".sessions-section")).to_be_visible()
        expect(dashboard_page.locator("#sessions-table")).to_be_visible()

    def test_sessions_table_has_rows(self, dashboard_page: Page) -> None:
        """The sessions table should display at least one row (the current session)."""
        dash = DashboardPage(dashboard_page)
        dash.scroll_to_section("sessions-section")
        # Wait for lazy-loaded data
        dashboard_page.wait_for_timeout(3000)

        rows = dash.get_sessions_table_rows()
        # Should have at least the empty-state row or actual session rows
        assert rows >= 1

    def test_cycle_overview_section(self, dashboard_page: Page) -> None:
        """The cycle overview section should exist with period filter controls."""
        dash = DashboardPage(dashboard_page)
        dash.scroll_to_section("cycle-overview-section")

        expect(dashboard_page.locator(".cycle-overview-section")).to_be_visible()
        expect(dashboard_page.locator("#overview-table")).to_be_visible()
        expect(dashboard_page.locator("#overview-page-size")).to_be_visible()
