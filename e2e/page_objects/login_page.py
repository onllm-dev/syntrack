"""Page object for the onWatch login page."""
from playwright.sync_api import Page


BASE_URL = "http://localhost:19211"


class LoginPage:
    """Wraps interactions with the /login page."""

    def __init__(self, page: Page) -> None:
        self.page = page

    def goto(self) -> None:
        """Navigate to the login page."""
        self.page.goto(f"{BASE_URL}/login")
        self.page.wait_for_selector(".login-card", timeout=10000)

    def login(self, username: str, password: str) -> None:
        """Fill in credentials and submit the login form."""
        self.page.fill("#username", username)
        self.page.fill("#password", password)
        self.page.click("button.login-button")

    def get_error_message(self) -> str:
        """Return the visible error message text, or empty string if none."""
        el = self.page.query_selector(".error-message")
        if el:
            return el.inner_text().strip()
        return ""

    def toggle_password_visibility(self) -> None:
        """Click the password visibility toggle button."""
        self.page.click("button.toggle-password")

    def get_password_input_type(self) -> str:
        """Return the current type attribute of the password input."""
        return self.page.get_attribute("#password", "type") or ""

    def is_visible(self) -> bool:
        """Check if the login card is visible."""
        return self.page.is_visible(".login-card")

    def get_title(self) -> str:
        """Return the page title."""
        return self.page.title()
