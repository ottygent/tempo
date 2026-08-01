const CACHE_NAME = "tempo-shell-v3";
const IS_LOCAL = self.location.hostname === "127.0.0.1" || self.location.hostname === "localhost";
const APP_SHELL = [
  "/",
  "/manifest.webmanifest",
  "/icons/tempo-192.png",
  "/icons/tempo-512.png",
  "/icons/tempo-maskable-512.png"
];

self.addEventListener("install", event => {
  if (!IS_LOCAL) event.waitUntil(caches.open(CACHE_NAME).then(cache => cache.addAll(APP_SHELL)));
  self.skipWaiting();
});

self.addEventListener("activate", event => {
  event.waitUntil(
    caches.keys()
      .then(keys => Promise.all(keys.filter(key => key.startsWith("tempo-") && (IS_LOCAL || key !== CACHE_NAME)).map(key => caches.delete(key))))
      .then(() => self.clients.claim())
      .then(() => IS_LOCAL ? self.clients.matchAll({type: "window"}) : [])
      .then(clients => Promise.all(clients.map(client => client.navigate(client.url).catch(() => undefined))))
  );
});

self.addEventListener("fetch", event => {
  if (IS_LOCAL) return;

  const request = event.request;
  if (request.method !== "GET") return;

  const url = new URL(request.url);
  if (url.origin !== self.location.origin || url.pathname.startsWith("/api/") || url.pathname === "/sw.js") return;

  if (request.mode === "navigate") {
    event.respondWith(
      fetch(request)
        .then(response => {
          if (response.ok && response.type === "basic") {
            const responseClone = response.clone();
            caches.open(CACHE_NAME).then(cache => cache.put("/", responseClone));
          }
          return response;
        })
        .catch(() => caches.match("/"))
    );
    return;
  }

  if (url.pathname === "/manifest.webmanifest") {
    event.respondWith(
      fetch(request)
        .then(response => {
          if (response.ok && response.type === "basic") {
            const responseClone = response.clone();
            caches.open(CACHE_NAME).then(cache => cache.put(request, responseClone));
          }
          return response;
        })
        .catch(() => caches.match(request))
    );
    return;
  }

  event.respondWith(
    caches.match(request).then(cached => cached || fetch(request).then(response => {
      if (response.ok && response.type === "basic") {
        const responseClone = response.clone();
        caches.open(CACHE_NAME).then(cache => cache.put(request, responseClone));
      }
      return response;
    }))
  );
});
