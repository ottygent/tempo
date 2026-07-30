import { render } from "solid-js/web";
import App from "./App";
import "./styles.css";
import "./documents.css";
import "./features.css";

render(()=><App/>,document.getElementById("root")!);

if("serviceWorker" in navigator){window.addEventListener("load",()=>{void navigator.serviceWorker.register("/sw.js")})}
