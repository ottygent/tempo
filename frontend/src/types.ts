export type TaskStatus = "backlog" | "todo" | "progress" | "review" | "done";
export interface Workspace { id:string; name:string; color:string; createdAt:string }
export interface Project { id:string; workspaceId:string; name:string; description:string; color:string; status:string; startDate:string; dueDate:string; createdAt:string }
export interface Task { id:string; projectId:string; title:string; description:string; status:TaskStatus; priority:string; assignee:string; startDate:string; dueDate:string; estimateMinutes:number; tags:string[]; createdAt:string; updatedAt:string }
export interface TimeEntry { id:string; taskId:string; startedAt:string; stoppedAt?:string; durationSeconds:number; note:string }
export interface Document { id:string; projectId:string; title:string; content:string; createdAt:string; updatedAt:string }
export interface AppState { version:number; workspaces:Workspace[]; projects:Project[]; tasks:Task[]; timeEntries:TimeEntry[]; documents:Document[] }
