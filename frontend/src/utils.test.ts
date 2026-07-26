import { describe,expect,it } from "vitest";
import { dueTone, monthGrid, secondsLabel, taskSeconds } from "./utils";
import type { Task } from "./types";

describe("task utilities",()=>{
  it("formats and totals tracked time",()=>{expect(secondsLabel(3661)).toBe("1:01:01");expect(taskSeconds("a",[{id:"1",taskId:"a",startedAt:"2026-01-01T00:00:00Z",stoppedAt:"2026-01-01T00:01:00Z",durationSeconds:60,note:""}])).toBe(60)});
  it("builds six complete calendar weeks",()=>{const days=monthGrid(new Date(2026,6,1));expect(days).toHaveLength(42);expect(days[0]?.getDay()).toBe(0);expect(days[41]?.getDay()).toBe(6)});
  it("marks overdue open tasks",()=>{const task={status:"todo",dueDate:"2026-01-01"} as Task;expect(dueTone(task,new Date("2026-01-03"))).toBe("overdue")});
});
