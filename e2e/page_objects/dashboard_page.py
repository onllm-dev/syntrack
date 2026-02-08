"""Page object for the onWatch dashboard page."""
from playwright.sync_api import Page


BASE_URL = "http://localhost:19211"


class DashboardPage:
    """Wraps interactions with the main dashboard (/) page."""

    def __init__(self, page: Page) -> None:
        self.page = page

    def goto(self) -> None:
        """Navigate to the dashboard."""
        self.page.goto(f"{BASE_URL}/")
        self.page.wait_for_selector(".app-header", timeout=10000)

    def select_provider(self, name: str) -> None:
        """Click a provider tab by its visible text."""
        self.page.click(f'.provider-tab:has-text("{name}")')
        # Wait for page to reflect the new provider
        self.page.wait_for_load_state("networkidle", timeout=10000)

    def get_active_provider(self) -> str:
        """Return the data-provider attribute of the active provider tab."""
        tab = self.page.query_selector(".provider-tab.active")
        if tab:
            return tab.get_attribute("data-provider") or ""
        return ""

    def get_provider_tabs(self) -> list[str]:
        """Return a list of provider tab texts."""
        tabs = self.page.query_selector_all(".provider-tab")
        return [tab.inner_text().strip() for tab in tabs]

    def get_quota_cards(self) -> list[str]:
        """Return a list of quota card data-quota attributes visible on the page."""
        cards = self.page.query_selector_all("article.quota-card")
        return [card.get_attribute("data-quota") or "" for card in cards]

    def get_card_status(self, quota_name: str) -> str:
        """Return the data-status attribute of a status badge for a given quota card."""
        badge = self.page.query_selector(
            f'article.quota-card[data-quota="{quota_name}"] .status-badge'
        )
        if badge:
            return badge.get_attribute("data-status") or ""
        return ""

    def get_card_percentage(self, quota_name: str) -> str:
        """Return the percentage text displayed on a quota card."""
        el = self.page.query_selector(
            f'article.quota-card[data-quota="{quota_name}"] .usage-percent'
        )
        if el:
            return el.inner_text().strip()
        return ""

    def open_card_modal(self, quota_name: str) -> None:
        """Click a quota card to open its detail modal."""
        self.page.click(f'article.quota-card[data-quota="{quota_name}"]')
        self.page.wait_for_selector("#detail-modal:not([hidden])", timeout=5000)

    def close_modal(self) -> None:
        """Close the detail modal via the close button."""
        self.page.click("#modal-close")
        self.page.wait_for_selector("#detail-modal[hidden]", timeout=5000)

    def close_modal_by_escape(self) -> None:
        """Close the detail modal by pressing Escape."""
        self.page.keyboard.press("Escape")
        self.page.wait_for_selector("#detail-modal[hidden]", timeout=5000)

    def close_modal_by_overlay(self) -> None:
        """Close the detail modal by clicking the overlay background."""
        # Click on the overlay outside the modal-content
        self.page.click("#detail-modal", position={"x": 10, "y": 10})
        self.page.wait_for_selector("#detail-modal[hidden]", timeout=5000)

    def is_modal_visible(self) -> bool:
        """Check if the detail modal is visible (not hidden)."""
        modal = self.page.query_selector("#detail-modal")
        if modal:
            return not modal.get_attribute("hidden") == ""
        return False

    def get_modal_title(self) -> str:
        """Return the modal title text."""
        el = self.page.query_selector("#modal-title")
        if el:
            return el.inner_text().strip()
        return ""

    def toggle_theme(self) -> None:
        """Click the theme toggle button."""
        self.page.click("#theme-toggle")

    def get_current_theme(self) -> str:
        """Return the current theme from the html data-theme attribute."""
        return self.page.get_attribute("html", "data-theme") or ""

    def click_refresh(self) -> None:
        """Click the refresh button."""
        self.page.click("#refresh-btn")

    def get_last_updated(self) -> str:
        """Return the last updated text."""
        el = self.page.query_selector("#last-updated")
        if el:
            return el.inner_text().strip()
        return ""

    def select_chart_range(self, range_value: str) -> None:
        """Click a chart range button by its data-range attribute."""
        self.page.click(
            f'.chart-section .range-selector .range-btn[data-range="{range_value}"]'
        )

    def get_active_chart_range(self) -> str:
        """Return the data-range of the active chart range button."""
        btn = self.page.query_selector(
            ".chart-section .range-selector .range-btn.active"
        )
        if btn:
            return btn.get_attribute("data-range") or ""
        return ""

    def scroll_to_section(self, section_class: str) -> None:
        """Scroll a section into view by its class name."""
        self.page.eval_on_selector(
            f".{section_class}",
            "el => el.scrollIntoView({behavior: 'instant'})",
        )
        self.page.wait_for_timeout(500)

    def get_version_text(self) -> str:
        """Return the version text from the footer."""
        el = self.page.query_selector(".footer-brand")
        if el:
            return el.inner_text().strip()
        return ""

    def has_settings_link(self) -> bool:
        """Check if the settings link/button is present."""
        return self.page.is_visible("#settings-btn")

    def open_password_modal(self) -> None:
        """Open the password change modal."""
        self.page.click("#change-password-btn")
        self.page.wait_for_selector("#password-modal:not([hidden])", timeout=5000)

    def close_password_modal(self) -> None:
        """Close the password change modal."""
        self.page.click("#password-modal-close")

    def change_password(self, current: str, new: str, confirm: str) -> None:
        """Fill in the password change form and submit."""
        self.page.fill("#current-password", current)
        self.page.fill("#new-password", new)
        self.page.fill("#confirm-password", confirm)
        self.page.click("#password-submit-btn")

    def get_password_error(self) -> str:
        """Return the password error message text."""
        el = self.page.query_selector("#password-error")
        if el and not el.is_hidden():
            return el.inner_text().strip()
        return ""

    def get_password_success(self) -> str:
        """Return the password success message text."""
        el = self.page.query_selector("#password-success")
        if el and not el.is_hidden():
            return el.inner_text().strip()
        return ""

    def get_chart_canvas(self) -> bool:
        """Check if the chart canvas element exists."""
        return self.page.is_visible("#usage-chart")

    def get_cycles_table_rows(self) -> int:
        """Return the number of rows in the cycles table body."""
        rows = self.page.query_selector_all("#cycles-tbody tr")
        return len(rows)

    def get_sessions_table_rows(self) -> int:
        """Return the number of rows in the sessions table body."""
        rows = self.page.query_selector_all("#sessions-tbody tr")
        return len(rows)
