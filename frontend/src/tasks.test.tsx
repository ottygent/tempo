import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { TaskDetailsDrawer } from "./App";
import type { Task } from "./types";

afterEach(() => cleanup());

describe("task details drawer", () => {
  it("shows the selected task details and closes from its header", async () => {
    const task: Task = {
      id: "task-1",
      projectId: "project-1",
      title: "Prepare launch brief",
      description: "Capture the launch scope and acceptance criteria.",
      status: "progress",
      priority: "high",
      assignee: "Maya",
      startDate: "2026-08-01",
      dueDate: "2026-08-05",
      estimateMinutes: 90,
      tags: ["launch", "brief"],
      createdAt: "2026-08-01T08:00:00Z",
      updatedAt: "2026-08-01T09:00:00Z",
    };
    const close = vi.fn();

    render(() => <TaskDetailsDrawer task={task} close={close} />);

    expect(screen.getByRole("dialog", {name: `Task details: ${task.title}`})).toBeTruthy();
    expect(screen.getByText(task.description)).toBeTruthy();
    expect(screen.getByText("Maya")).toBeTruthy();
    expect(screen.getByText("1h 30m")).toBeTruthy();
    expect(screen.getByText("launch")).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", {name: "Close task details"}));
    expect(close).toHaveBeenCalledOnce();
  });
});
