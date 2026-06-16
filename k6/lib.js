import http from "k6/http";

export const BASE_URL = __ENV.BASE_URL || "http://localhost:8080";
export const TARGET_URL = __ENV.TARGET_URL || "https://example.com";
export const JSON_HEADERS = { "Content-Type": "application/json" };

export function createLink(url = TARGET_URL) {
  return http.post(`${BASE_URL}/links/`, JSON.stringify({ url }), {
    headers: JSON_HEADERS,
  });
}

export function createLinks(count, urlPrefix = TARGET_URL) {
  const slugs = [];
  for (let i = 0; i < count; i++) {
    const res = createLink(`${urlPrefix}?n=${i}`);
    if (res.status === 201) {
      slugs.push(res.json("data.slug"));
    }
  }
  return slugs;
}
