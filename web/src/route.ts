// Hash routes are the single source of truth for navigation, so every view
// has a shareable link and the browser's back button works.
//   #/                                fleet
//   #/node/<id>                       node investigation
//   #/compare/<id>?base=&candidate=&from=&to=   release comparison
import { useEffect, useState } from "react";

export type Route = { view: "fleet" | "node" | "compare"; node: string; params: URLSearchParams };

export function parse(hash = location.hash): Route {
  const [path, query = ""] = hash.replace(/^#/, "").split("?");
  const [, kind = "", id = ""] = path.split("/");
  const view = kind === "node" ? "node" : kind === "compare" ? "compare" : "fleet";
  return { view, node: decodeURIComponent(id), params: new URLSearchParams(query) };
}

export function href(view: Route["view"], node = "", params?: Record<string, string>): string {
  if (view === "fleet") return "#/";
  const query = params ? new URLSearchParams(params).toString() : "";
  return `#/${view}/${encodeURIComponent(node)}${query ? "?" + query : ""}`;
}

export function useRoute(): Route {
  const [route, setRoute] = useState(parse);
  useEffect(() => {
    const follow = () => setRoute(parse());
    window.addEventListener("hashchange", follow);
    return () => window.removeEventListener("hashchange", follow);
  }, []);
  return route;
}
