import { afterEach, describe, expect, it, vi } from "vitest";

describe("account settings API", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.resetModules();
  });

  it("uses the refreshed CSRF token after account settings change", async () => {
    const responses = [
      { authenticated: true, username: "admin", email: "", csrfToken: "csrf-before" },
      { authenticated: true, username: "owner", email: "owner@example.com", csrfToken: "csrf-after" },
      { id: "wrk_test", name: "Test", color: "#7157e8", createdAt: "2026-07-30T00:00:00Z" },
    ];
    const fetchMock = vi.fn(async () => new Response(JSON.stringify(responses.shift()), {
      headers: { "content-type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetchMock);
    const { api } = await import("./api");

    await api.session();
    await api.updateSettings({
      username: "owner",
      email: "owner@example.com",
      currentPassword: "correct-horse-battery-staple",
    });
    await api.createWorkspace({ name: "Test", color: "#7157e8" });

    expect(fetchMock).toHaveBeenNthCalledWith(2, "/api/auth/settings", expect.objectContaining({
      method: "PATCH",
      headers: expect.objectContaining({ "x-csrf-token": "csrf-before" }),
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, "/api/workspaces", expect.objectContaining({
      method: "POST",
      headers: expect.objectContaining({ "x-csrf-token": "csrf-after" }),
    }));
  });

  it("resynchronizes a stale CSRF token before retrying a mutation", async () => {
    const responses = [
      new Response(JSON.stringify({ authenticated: true, username: "admin", csrfToken: "csrf-stale" })),
      new Response(JSON.stringify({ error: "invalid CSRF token" }), { status: 403 }),
      new Response(JSON.stringify({ authenticated: true, username: "owner", csrfToken: "csrf-current" })),
      new Response(JSON.stringify({ id: "wrk_retry", name: "Retry", color: "#7157e8", createdAt: "2026-07-30T00:00:00Z" })),
    ];
    const fetchMock = vi.fn(async () => responses.shift()!);
    vi.stubGlobal("fetch", fetchMock);
    const { api } = await import("./api");

    await api.session();
    await api.createWorkspace({ name: "Retry", color: "#7157e8" });

    expect(fetchMock).toHaveBeenNthCalledWith(2, "/api/workspaces", expect.objectContaining({
      headers: expect.objectContaining({ "x-csrf-token": "csrf-stale" }),
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, "/api/auth/session", expect.objectContaining({
      credentials: "same-origin",
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(4, "/api/workspaces", expect.objectContaining({
      headers: expect.objectContaining({ "x-csrf-token": "csrf-current" }),
    }));
  });
});
