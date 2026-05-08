import { getApplication, getApplications, getScenarios } from '$lib/api';

import type { PageLoad } from './$types';

export const load: PageLoad = async ({ fetch, url }) => {
  const [scenarios, applications] = await Promise.all([
    getScenarios(fetch).catch(() => []),
    getApplications(fetch).catch(() => []),
  ]);

  const applicationId = url.searchParams.get('applicationId') ?? '';
  const runId = url.searchParams.get('runId') ?? '';
  const app = applicationId
    ? await getApplication(applicationId, runId || undefined, fetch).catch(
        () => null,
      )
    : null;

  return { scenarios, applications, app, applicationId, runId };
};
