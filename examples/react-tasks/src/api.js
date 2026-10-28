// Calls to the app's own server (server/server.js). Every function returns a promise and
// throws an Error with the server's message when the request fails.
async function request(method, path, body) {
  const res = await fetch(`/api${path}`, {
    method,
    headers: body ? { "Content-Type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  if (res.status === 204) return null;
  const data = await res.json().catch(() => null);
  // Nimbus answers 401 not_signed_in when the viewer's sign-in has expired.
  // Reloading the page sends them through sign-in and back here.
  if (res.status === 401 && data?.error === "not_signed_in") {
    window.location.reload();
    return new Promise(() => {}); // never settles; the page is reloading
  }
  if (!res.ok) throw new Error(data?.error ?? `${method} /api${path} failed with ${res.status}`);
  return data;
}

export const api = {
  me: () => request("GET", "/me"), // { email }
  board: () => request("GET", "/board"), // { columns: [{ id, title, cards: [...] }] }
  createCard: (title, column) => request("POST", "/cards", { title, column }),
  updateCard: (id, changes) => request("PATCH", `/cards/${id}`, changes), // { title?, column?, index? }
  deleteCard: (id) => request("DELETE", `/cards/${id}`),
};
