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

    render(() => <TaskDetailsDrawer task={task} close={close} save={vi.fn()} />);

    expect(screen.getByRole("dialog", {name: `Task details: ${task.title}`})).toBeTruthy();
    expect(screen.getByText(task.description)).toBeTruthy();
    expect(screen.getByText("Maya")).toBeTruthy();
    expect(screen.getByText("1h 30m")).toBeTruthy();
    expect(screen.getByText("launch")).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", {name: "Close task details"}));
    expect(close).toHaveBeenCalledOnce();
  });

  it("edits and saves task details without closing the drawer", async () => {
    const task: Task = {
      id: "task-1",
      projectId: "project-1",
      title: "Prepare launch brief",
      description: "Initial scope",
      status: "todo",
      priority: "medium",
      assignee: "Maya",
      startDate: "2026-08-01",
      dueDate: "2026-08-05",
      estimateMinutes: 60,
      tags: ["launch"],
      createdAt: "2026-08-01T08:00:00Z",
      updatedAt: "2026-08-01T09:00:00Z",
    };
    const save = vi.fn(async(input:Partial<Task>)=>({...task,...input,updatedAt:"2026-08-01T10:00:00Z"}));

    render(() => <TaskDetailsDrawer task={task} close={vi.fn()} save={save} />);
    await fireEvent.click(screen.getByRole("button", {name: "Edit"}));
    await fireEvent.input(screen.getByLabelText("Task title"), {target: {value: "Publish launch brief"}});
    await fireEvent.change(screen.getByLabelText("Status"), {target: {value: "progress"}});
    await fireEvent.change(screen.getByLabelText("Priority"), {target: {value: "high"}});
    await fireEvent.input(screen.getByLabelText("Tags"), {target: {value: "launch, writing"}});
    await fireEvent.click(screen.getByRole("button", {name: "Save changes"}));

    expect(save).toHaveBeenCalledWith(expect.objectContaining({
      title: "Publish launch brief",
      status: "progress",
      priority: "high",
      tags: ["launch", "writing"],
    }));
    expect(screen.queryByRole("button", {name: "Save changes"})).toBeNull();
    expect(screen.getByRole("button", {name: "Edit"})).toBeTruthy();
  });
});
