import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ProfileModal, SettingsModal } from "./App";

describe("account settings form", () => {
  afterEach(() => cleanup());

  it("allows a legacy account to save without an email address", async () => {
    const save = vi.fn().mockResolvedValue({
      authenticated: true,
      username: "owner",
      email: "",
      csrfToken: "fresh",
    });
    const saved = vi.fn();
    render(() => <SettingsModal
      username="admin"
      email=""
      close={vi.fn()}
      save={save}
      saved={saved}
    />);

    await fireEvent.input(document.querySelector<HTMLInputElement>('input[name="username"]')!, { target: { value: "owner" } });
    await fireEvent.input(screen.getByLabelText("Current password"), { target: { value: "correct-horse-battery-staple" } });
    await fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(save).toHaveBeenCalledWith({
      username: "owner",
      email: "",
      currentPassword: "correct-horse-battery-staple",
    }));
    expect(saved).toHaveBeenCalled();
  });

  it("counts Unicode code points for the password minimum", async () => {
    const save = vi.fn();
    render(() => <SettingsModal
      username="admin"
      email=""
      close={vi.fn()}
      save={save}
      saved={vi.fn()}
    />);

    const shortPassword = "🙂".repeat(11);
    await fireEvent.input(screen.getByLabelText("Current password"), { target: { value: "correct-horse-battery-staple" } });
    await fireEvent.input(document.querySelector<HTMLInputElement>('input[name="newPassword"]')!, { target: { value: shortPassword } });
    await fireEvent.input(document.querySelector<HTMLInputElement>('input[name="confirmPassword"]')!, { target: { value: shortPassword } });
    await fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    expect((await screen.findByRole("alert")).textContent).toContain("at least 12 characters");
    expect(save).not.toHaveBeenCalled();
  });
});

describe("profile modal", () => {
  afterEach(() => cleanup());

  it("groups profile, account, settings, and logout behind the avatar", async () => {
    const settings = vi.fn(), logout = vi.fn();
    render(() => <ProfileModal username="admin" email="owner@example.com" close={vi.fn()} settings={settings} logout={logout}/>);

    expect(screen.getByRole("dialog", { name: "Profile & account" })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Profile/ }).getAttribute("aria-current")).toBe("page");
    expect(screen.getByRole("heading", { name: "admin" })).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: /Account/ }));
    expect(screen.getByRole("heading", { name: "Account details" })).toBeTruthy();
    expect(screen.getByText("owner@example.com")).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: /Settings/ }));
    await fireEvent.click(screen.getByRole("button", { name: /Logout/ }));
    expect(settings).toHaveBeenCalledOnce();
    expect(logout).toHaveBeenCalledOnce();
  });
});
