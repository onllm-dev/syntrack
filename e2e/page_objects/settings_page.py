"""Page object for the onWatch settings page."""
from typing import Optional

from playwright.sync_api import Page


BASE_URL = "http://localhost:19211"


class SettingsPage:
    """Wraps interactions with the /settings page."""

    def __init__(self, page: Page) -> None:
        self.page = page

    def goto(self) -> None:
        """Navigate to the settings page."""
        self.page.goto(f"{BASE_URL}/settings")
        self.page.wait_for_selector(".settings-page", timeout=10000)

    def select_tab(self, tab_name: str) -> None:
        """Click a settings tab by its data-tab attribute."""
        self.page.click(f'.settings-tab[data-tab="{tab_name}"]')
        self.page.wait_for_selector(f"#panel-{tab_name}:not([hidden])", timeout=5000)

    def get_active_tab(self) -> str:
        """Return the data-tab attribute of the active settings tab."""
        tab = self.page.query_selector(".settings-tab.active")
        if tab:
            return tab.get_attribute("data-tab") or ""
        return ""

    def get_tab_names(self) -> list[str]:
        """Return a list of all settings tab texts."""
        tabs = self.page.query_selector_all(".settings-tab")
        return [tab.inner_text().strip() for tab in tabs]

    def configure_smtp(self, config: dict) -> None:
        """Fill in SMTP configuration fields."""
        field_map = {
            "host": "#smtp-host",
            "port": "#smtp-port",
            "username": "#smtp-username",
            "password": "#smtp-password",
            "from_address": "#smtp-from-address",
            "from_name": "#smtp-from-name",
            "to": "#smtp-to",
        }
        for key, selector in field_map.items():
            if key in config:
                self.page.fill(selector, str(config[key]))

        if "protocol" in config:
            self.page.select_option("#smtp-protocol", config["protocol"])

    def send_test_email(self) -> None:
        """Click the 'Send Test Email' button."""
        self.page.click("#smtp-test-btn")

    def get_test_email_result(self) -> str:
        """Return the test email result text."""
        el = self.page.query_selector("#smtp-test-result")
        if el:
            return el.inner_text().strip()
        return ""

    def set_warning_threshold(self, value: int) -> None:
        """Set the warning threshold input value."""
        self.page.fill("#threshold-warning", str(value))

    def set_critical_threshold(self, value: int) -> None:
        """Set the critical threshold input value."""
        self.page.fill("#threshold-critical", str(value))

    def get_warning_slider_value(self) -> str:
        """Return the warning slider current value."""
        return self.page.input_value("#threshold-warning-slider")

    def get_critical_slider_value(self) -> str:
        """Return the critical slider current value."""
        return self.page.input_value("#threshold-critical-slider")

    def get_warning_input_value(self) -> str:
        """Return the warning input current value."""
        return self.page.input_value("#threshold-warning")

    def get_critical_input_value(self) -> str:
        """Return the critical input current value."""
        return self.page.input_value("#threshold-critical")

    def is_provider_toggles_visible(self) -> bool:
        """Check if the provider toggles section is visible."""
        return self.page.is_visible("#provider-toggles")

    def get_timezone_select(self) -> Optional[str]:
        """Return the current timezone select value."""
        return self.page.input_value("#settings-timezone")

    def save_settings(self) -> None:
        """Click the global Save Settings button."""
        self.page.click("#settings-save-btn")

    def get_feedback(self) -> str:
        """Return the settings feedback message text."""
        el = self.page.query_selector("#settings-feedback")
        if el and not el.is_hidden():
            return el.inner_text().strip()
        return ""

    def is_panel_visible(self, panel_name: str) -> bool:
        """Check if a settings panel is visible (not hidden)."""
        panel = self.page.query_selector(f"#panel-{panel_name}")
        if panel:
            return not panel.is_hidden()
        return False
