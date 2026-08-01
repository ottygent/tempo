import { cleanup, fireEvent, render, screen, waitFor, within } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DocumentsView, EditableMarkdown } from "./App";
import type { Document, Project } from "./types";

afterEach(() => cleanup());

describe("document editor", () => {
  it("clears the empty state and preserves root-level text input", async () => {
    const onChange = vi.fn();
    render(() => <EditableMarkdown documentId="doc-1" content="" onChange={onChange} />);

    const editor = screen.getByRole("textbox", { name: "Document content" });
    await waitFor(() => expect(editor.getAttribute("data-empty")).toBe("true"));

    editor.textContent = "A real first line";
    await fireEvent.input(editor);

    expect(editor.getAttribute("data-empty")).toBe("false");
    expect(onChange).toHaveBeenLastCalledWith("A real first line");
  });

  it("keeps the sidebar filename-only and shows a compact editor filename header", async () => {
    const project: Project = {
      id: "project-1",
      workspaceId: "workspace-1",
      name: "Launch",
      description: "",
      color: "#6c52e3",
      status: "active",
      startDate: "",
      dueDate: "",
      createdAt: "2026-07-31T00:00:00Z",
    };
    const projectDocument: Document = {
      id: "doc-1",
      projectId: project.id,
      title: "Research notes.md",
      content: "Private body preview that must not appear in the sidebar",
      createdAt: "2026-07-31T00:00:00Z",
      updatedAt: "2026-07-31T00:00:00Z",
    };

    render(() => <DocumentsView
      documents={[projectDocument]}
      project={project}
      createRequest={0}
      consumeCreateRequest={vi.fn()}
      create={vi.fn()}
      save={vi.fn()}
      remove={vi.fn()}
    />);

    await waitFor(() => expect(document.querySelector(".document-editor-header strong")?.textContent).toBe(projectDocument.title));
    const library = document.querySelector(".document-library");
    expect(library).toBeTruthy();
    expect(within(library as HTMLElement).getByText(projectDocument.title)).toBeTruthy();
    expect(within(library as HTMLElement).queryByText(projectDocument.content)).toBeNull();
    expect(document.querySelector(".document-editor-header")?.classList.contains("document-editor-header")).toBe(true);
  });

  it("renames a document inline after double-clicking its editor filename", async () => {
    const project: Project = {
      id: "project-1",
      workspaceId: "workspace-1",
      name: "Launch",
      description: "",
      color: "#6c52e3",
      status: "active",
      startDate: "",
      dueDate: "",
      createdAt: "2026-07-31T00:00:00Z",
    };
    const projectDocument: Document = {
      id: "doc-1",
      projectId: project.id,
      title: "Untitled document",
      content: "Existing content",
      createdAt: "2026-07-31T00:00:00Z",
      updatedAt: "2026-07-31T00:00:00Z",
    };
    const save = vi.fn(async (id:string,title:string,content:string) => ({...projectDocument,id,title,content}));

    render(() => <DocumentsView
      documents={[projectDocument]}
      project={project}
      createRequest={0}
      consumeCreateRequest={vi.fn()}
      create={vi.fn()}
      save={save}
      remove={vi.fn()}
    />);

    const filename = await screen.findByRole("button", {name: "Rename Untitled document"});
    await fireEvent.dblClick(filename);
    const input = screen.getByRole("textbox", {name: "Document filename"});
    await fireEvent.input(input, {target: {value: "Project brief"}});
    await fireEvent.keyDown(input, {key: "Enter"});

    await waitFor(() => expect(save).toHaveBeenCalledWith("doc-1", "Project brief", "Existing content"));
    expect(screen.getByRole("button", {name: "Rename Project brief"})).toBeTruthy();
  });
});
