"""E2E tests for quota cards.

7 tests covering card rendering, progress bars, status badges, and countdown.
"""
import pytest
from playwright.sync_api import Page, expect

from page_objects.dashboard_page import DashboardPage

BASE_URL = "http://localhost:19211"


class TestQuotaCards:
    """Quota card display and interaction tests."""

    def test_anthropic_dynamic_cards_render(self, dashboard_page: Page) -> None:
        """Anthropic provider view should have a dynamic quota grid container.

        Note: Cards are populated by JS from API data. If the Anthropic API
        is unavailable (E2E mock limitation), the grid exists but may be empty.
        We verify the container is present and correctly wired.
        """
        dash = DashboardPage(dashboard_page)

        # Ensure Anthropic tab is active
        if dash.get_active_provider() != "anthropic":
            dash.select_provider("Anthropic")

        # The Anthropic quota grid container should exist in the DOM
        dashboard_page.wait_for_timeout(1000)
        grid = dashboard_page.query_selector("#quota-grid-anthropic")
        assert grid is not None, "Anthropic quota grid container should exist"
        assert grid.get_attribute("data-provider") == "anthropic"

    def test_synthetic_fixed_cards(self, dashboard_page: Page) -> None:
        """Synthetic provider should render its 3 fixed quota cards."""
        dash = DashboardPage(dashboard_page)

        tabs = dash.get_provider_tabs()
        if not any("Synthetic" in t for t in tabs):
            pytest.skip("Synthetic provider not configured")

        dash.select_provider("Synthetic")
        dashboard_page.wait_for_timeout(1000)

        cards = dash.get_quota_cards()
        assert "subscription" in cards
        assert "search" in cards
        assert "toolCalls" in cards

    def test_zai_tokens_and_time_cards(self, dashboard_page: Page) -> None:
        """Z.ai provider should render tokens limit and time limit cards."""
        dash = DashboardPage(dashboard_page)

        tabs = dash.get_provider_tabs()
        if not any("Z.ai" in t for t in tabs):
            pytest.skip("Z.ai provider not configured")

        dash.select_provider("Z.ai")
        dashboard_page.wait_for_timeout(1000)

        cards = dash.get_quota_cards()
        assert "tokensLimit" in cards
        assert "timeLimit" in cards

    def test_progress_bar_exists(self, dashboard_page: Page) -> None:
        """Each quota card should contain a progress bar element."""
        dash = DashboardPage(dashboard_page)

        # Switch to Synthetic which has static HTML cards (always present)
        tabs = dash.get_provider_tabs()
        if any("Synthetic" in t for t in tabs):
            dash.select_provider("Synthetic")
        elif any("Z.ai" in t for t in tabs):
            dash.select_provider("Z.ai")
        dashboard_page.wait_for_timeout(1000)

        progress_bars = dashboard_page.query_selector_all(
            "article.quota-card .progress-fill"
        )
        assert len(progress_bars) >= 1

    def test_status_badges_present(self, dashboard_page: Page) -> None:
        """Each quota card should display a status badge with a data-status attribute."""
        dash = DashboardPage(dashboard_page)

        # Switch to Synthetic which has static HTML cards
        tabs = dash.get_provider_tabs()
        if any("Synthetic" in t for t in tabs):
            dash.select_provider("Synthetic")
        elif any("Z.ai" in t for t in tabs):
            dash.select_provider("Z.ai")
        dashboard_page.wait_for_timeout(1000)

        badges = dashboard_page.query_selector_all("article.quota-card .status-badge")
        assert len(badges) >= 1

        for badge in badges:
            status = badge.get_attribute("data-status")
            assert status in ("healthy", "warning", "danger", "critical")

    def test_countdown_present(self, dashboard_page: Page) -> None:
        """Quota cards should display a countdown timer element."""
        dash = DashboardPage(dashboard_page)

        # Switch to Synthetic which has static HTML cards
        tabs = dash.get_provider_tabs()
        if any("Synthetic" in t for t in tabs):
            dash.select_provider("Synthetic")
        elif any("Z.ai" in t for t in tabs):
            dash.select_provider("Z.ai")
        dashboard_page.wait_for_timeout(1000)

        countdowns = dashboard_page.query_selector_all("article.quota-card .countdown")
        assert len(countdowns) >= 1

    def test_card_click_opens_modal(self, dashboard_page: Page) -> None:
        """Clicking a quota card should open the detail modal."""
        dash = DashboardPage(dashboard_page)

        # Switch to Synthetic to have known fixed cards
        tabs = dash.get_provider_tabs()
        if any("Synthetic" in t for t in tabs):
            dash.select_provider("Synthetic")
            dashboard_page.wait_for_timeout(1000)
            dash.open_card_modal("subscription")
            assert dash.is_modal_visible()
            dash.close_modal()
        elif any("Z.ai" in t for t in tabs):
            dash.select_provider("Z.ai")
            dashboard_page.wait_for_timeout(1000)
            dash.open_card_modal("tokensLimit")
            assert dash.is_modal_visible()
            dash.close_modal()
        else:
            pytest.skip("No provider with fixed cards available")
