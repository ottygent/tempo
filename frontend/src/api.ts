import type { AppState, Project, Task, TimeEntry, Workspace } from "./types";

export interface AuthSession { authenticated:boolean; username?:string; csrfToken?:string; expires?:number }
export class ApiError extends Error { constructor(message:string,public readonly status:number){super(message)} }
let csrfToken="";

async function call<T>(url:string, init?:RequestInit):Promise<T>{
  const headers:Record<string,string>={"content-type":"application/json",...(init?.headers as Record<string,string>|undefined)};
  if(init?.method&&!["GET","HEAD","OPTIONS"].includes(init.method.toUpperCase())&&csrfToken)headers["x-csrf-token"]=csrfToken;
  const response=await fetch(url,{...init,credentials:"same-origin",headers});
  if(!response.ok){const body=await response.json().catch(()=>({error:response.statusText})) as {error?:string};throw new ApiError(body.error??response.statusText,response.status)}
  return response.json() as Promise<T>;
}
function remember(session:AuthSession){csrfToken=session.csrfToken??"";return session}
export const api={
  session:async()=>remember(await call<AuthSession>("/api/auth/session")),
  login:async(username:string,password:string)=>remember(await call<AuthSession>("/api/auth/login",{method:"POST",body:JSON.stringify({username,password})})),
  logout:async()=>{const result=await call<AuthSession>("/api/auth/logout",{method:"POST",body:"{}"});csrfToken="";return result},
  state:()=>call<AppState>("/api/state"),
  createWorkspace:(body:Partial<Workspace>)=>call<Workspace>("/api/workspaces",{method:"POST",body:JSON.stringify(body)}),
  createProject:(body:Partial<Project>)=>call<Project>("/api/projects",{method:"POST",body:JSON.stringify(body)}),
  createTask:(body:Partial<Task>)=>call<Task>("/api/tasks",{method:"POST",body:JSON.stringify(body)}),
  updateTask:(id:string,body:Partial<Task>)=>call<Task>(`/api/tasks/${id}`,{method:"PATCH",body:JSON.stringify(body)}),
  startTimer:(taskId:string)=>call<TimeEntry>("/api/time/start",{method:"POST",body:JSON.stringify({taskId})}),
  stopTimer:()=>call<TimeEntry>("/api/time/stop",{method:"POST",body:"{}"})
};
