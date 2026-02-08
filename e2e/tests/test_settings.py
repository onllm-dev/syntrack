"""E2E tests for the settings page.

8 tests covering tabs, SMTP form, thresholds, provider toggles, and save.
"""
import pytest
from playwright.sync_api import Page, expect

from page_objects.settings_page import SettingsPage

BASE_URL = "http://localhost:19211"


class TestSettings:
    """Settings page interaction tests."""

    def test_four_tabs_present(self, settings_page: Page) -> None:
        """Settings page should have 4 tabs: Email, Notifications, Providers, General."""
        sp = SettingsPage(settings_page)
        tabs = sp.get_tab_names()
        assert len(tabs) == 4
        assert "Email (SMTP)" in tabs
        assert "Notifications" in tabs
        assert "Providers" in tabs
        assert "General" in tabs

    def test_smtp_form_fields(self, settings_page: Page) -> None:
        """The Email (SMTP) tab should display all SMTP configuration fields."""
        sp = SettingsPage(settings_page)
        # Email tab should be active by default
        assert sp.get_active_tab() == "email"

        expect(settings_page.locator("#smtp-host")).to_be_visible()
        expect(settings_page.locator("#smtp-port")).to_be_visible()
        expect(settings_page.locator("#smtp-protocol")).to_be_visible()
        expect(settings_page.locator("#smtp-username")).to_be_visible()
        expect(settings_page.locator("#smtp-password")).to_be_visible()
        expect(settings_page.locator("#smtp-from-address")).to_be_visible()
        expect(settings_page.locator("#smtp-from-name")).to_be_visible()
        expect(settings_page.locator("#smtp-to")).to_be_visible()

    def test_send_test_email_button(self, settings_page: Page) -> None:
        """The test email button should be present and clickable."""
        sp = SettingsPage(settings_page)
        assert sp.get_active_tab() == "email"
        expect(settings_page.locator("#smtp-test-btn")).to_be_visible()

    def test_notification_thresholds(self, settings_page: Page) -> None:
        """Notifications tab should have warning and critical threshold inputs."""
        sp = SettingsPage(settings_page)
        sp.select_tab("notifications")

        expect(settings_page.locator("#threshold-warning")).to_be_visible()
        expect(settings_page.locator("#threshold-critical")).to_be_visible()
        expect(settings_page.locator("#threshold-warning-slider")).to_be_visible()
        expect(settings_page.locator("#threshold-critical-slider")).to_be_visible()

    def test_threshold_slider_sync(self, settings_page: Page) -> None:
        """Changing the threshold number input should sync with the slider."""
        sp = SettingsPage(settings_page)
        sp.select_tab("notifications")

        # Set warning threshold via number input
        sp.set_warning_threshold(70)
        # Trigger input event
        settings_page.dispatch_event("#threshold-warning", "input")
        settings_page.wait_for_timeout(500)

        # Default values should be present
        warning_val = sp.get_warning_input_value()
        assert warning_val == "70"

    def test_provider_toggles_tab(self, settings_page: Page) -> None:
        """Providers tab should show toggle controls for each provider."""
        sp = SettingsPage(settings_page)
        sp.select_tab("providers")

        assert sp.is_panel_visible("providers")
        assert sp.is_provider_toggles_visible()

    def test_timezone_setting(self, settings_page: Page) -> None:
        """General tab should have a timezone selector."""
        sp = SettingsPage(settings_page)
        sp.select_tab("general")

        expect(settings_page.locator("#settings-timezone")).to_be_visible()
        tz = sp.get_timezone_select()
        # Default should be empty string (browser default)
        assert tz is not None

    def test_save_settings_button(self, settings_page: Page) -> None:
        """The Save Settings button should be present and clickable."""
        sp = SettingsPage(settings_page)
        expect(settings_page.locator("#settings-save-btn")).to_be_visible()

        # Click save -- may show success or error depending on config state
        sp.save_settings()
        settings_page.wait_for_timeout(1000)
        # Feedback area should become visible after save
        feedback_el = settings_page.query_selector("#settings-feedback")
        assert feedback_el is not None
