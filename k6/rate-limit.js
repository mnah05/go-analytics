import http from 'k6/http';
import { check } from 'k6';
import { Rate } from 'k6/metrics';

const failures = new Rate('failed_requests');
const rateLimited = new Rate('rate_limited');

export const options = {
  vus: 1,
  duration: '30s',
  thresholds: {
    // At least 50% of requests should be rate limited (429) when exceeding 10 req/s
    rate_limited: ['rate>0.5'],
    failed_requests: ['rate<1.0'],
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

export default function () {
  // Fire requests as fast as possible from a single IP to trigger rate limiting
  const res = http.get(`${BASE_URL}/health`);

  const isLimited = res.status === 429;
  rateLimited.add(isLimited);

  // 429 is expected - not a "failure" per se, but anything else unexpected is
  if (res.status !== 429) {
    check(res, {
      'non-rate-limited status is 200': (r) => r.status === 200,
    });
    failures.add(res.status !== 200);
  }
}
