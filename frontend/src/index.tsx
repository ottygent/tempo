import { render } from "solid-js/web";
import App from "./App";
import "./styles.css";
import "./documents.css";
import "./features.css";

render(()=><App/>,document.getElementById("root")!);

if("serviceWorker" in navigator){
  window.addEventListener("load",()=>{
    if(import.meta.env.DEV){
      void Promise.all([
        navigator.serviceWorker.getRegistrations().then(registrations=>Promise.all(registrations.map(registration=>registration.unregister()))),
        caches.keys().then(keys=>Promise.all(keys.filter(key=>key.startsWith("tempo-")).map(key=>caches.delete(key))))
      ]);
      return;
    }
    void navigator.serviceWorker.register("/sw.js");
  });
}
