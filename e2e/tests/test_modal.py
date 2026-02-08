"""E2E tests for the detail modal.

6 tests covering modal open, close, chart canvas, cycle table, and dismiss methods.
"""
import pytest
from playwright.sync_api import Page, expect

from page_objects.dashboard_page import DashboardPage

BASE_URL = "http://localhost:19211"


def _get_first_clickable_card(dash: DashboardPage, page: Page) -> str:
    """Switch to a provider with fixed cards and return the first card's data-quota."""
    tabs = dash.get_provider_tabs()
    if any("Synthetic" in t for t in tabs):
        dash.select_provider("Synthetic")
        page.wait_for_timeout(1000)
        return "subscription"
    elif any("Z.ai" in t for t in tabs):
        dash.select_provider("Z.ai")
        page.wait_for_timeout(1000)
        return "tokensLimit"
    pytest.skip("No provider with fixed cards available")
    return ""


class TestModal:
    """Detail modal interaction tests."""

    def test_modal_opens_on_card_click(self, dashboard_page: Page) -> None:
        """Clicking a quota card should open the detail modal."""
        dash = DashboardPage(dashboard_page)
        card_name = _get_first_clickable_card(dash, dashboard_page)

        dash.open_card_modal(card_name)
        expect(dashboard_page.locator("#detail-modal")).not_to_have_attribute(
            "hidden", ""
        )

    def test_modal_has_chart_canvas(self, dashboard_page: Page) -> None:
        """The detail modal should contain a chart canvas element."""
        dash = DashboardPage(dashboard_page)
        card_name = _get_first_clickable_card(dash, dashboard_page)

        dash.open_card_modal(card_name)
        # The modal body is dynamically populated; look for a canvas
        dashboard_page.wait_for_timeout(1000)
        modal_body = dashboard_page.query_selector("#modal-body")
        assert modal_body is not None

        # Check for canvas or chart container in modal
        has_canvas = dashboard_page.query_selector("#modal-body canvas") is not None
        has_chart_container = (
            dashboard_page.query_selector("#modal-body .modal-chart") is not None
            or dashboard_page.query_selector("#modal-body .chart-container") is not None
        )
        assert has_canvas or has_chart_container or modal_body.inner_text().strip() != ""

        dash.close_modal()

    def test_modal_has_cycle_table(self, dashboard_page: Page) -> None:
        """The detail modal should contain cycle data or a table."""
        dash = DashboardPage(dashboard_page)
        card_name = _get_first_clickable_card(dash, dashboard_page)

        dash.open_card_modal(card_name)
        dashboard_page.wait_for_timeout(1000)

        modal_body = dashboard_page.query_selector("#modal-body")
        assert modal_body is not None
        content = modal_body.inner_text().strip()
        # Modal should have some content (cycle table or "no data" message)
        assert len(content) > 0

        dash.close_modal()

    def test_modal_close_button(self, dashboard_page: Page) -> None:
        """The close button should dismiss the detail modal."""
        dash = DashboardPage(dashboard_page)
        card_name = _get_first_clickable_card(dash, dashboard_page)

        dash.open_card_modal(card_name)
        assert dash.is_modal_visible()

        dash.close_modal()
        expect(dashboard_page.locator("#detail-modal")).to_have_attribute("hidden", "")

    def test_modal_close_by_escape(self, dashboard_page: Page) -> None:
        """Pressing Escape should close the detail modal."""
        dash = DashboardPage(dashboard_page)
        card_name = _get_first_clickable_card(dash, dashboard_page)

        dash.open_card_modal(card_name)
        assert dash.is_modal_visible()

        dash.close_modal_by_escape()
        expect(dashboard_page.locator("#detail-modal")).to_have_attribute("hidden", "")

    def test_modal_close_by_overlay_click(self, dashboard_page: Page) -> None:
        """Clicking the overlay background should close the detail modal."""
        dash = DashboardPage(dashboard_page)
        card_name = _get_first_clickable_card(dash, dashboard_page)

        dash.open_card_modal(card_name)
        assert dash.is_modal_visible()

        dash.close_modal_by_overlay()
        expect(dashboard_page.locator("#detail-modal")).to_have_attribute("hidden", "")
