import { describe, expect, test } from 'bun:test';
import { check, dockerBase, type DockerContainer, type HubNode } from './cutover-check';

const c = (name: string, project: string | undefined, file: string, status = 'Up 2 hours (healthy)', state = 'running'): DockerContainer => ({
  Names: [`/${name}`],
  State: state,
  Status: status,
  Labels: project ? { 'com.docker.compose.project': project, 'com.docker.compose.project.config_files': file } : {},
});
const node = (over: Partial<HubNode> = {}): HubNode => ({
  node: 'n_1',
  name: 'pvedocker',
  online: true,
  stacks: [{ name: 'media', status: 'running', path: '/opt/stacks/media', file: 'compose.yaml', drift: false, head: { short: 'abc1234' } }],
  detected: [],
  ...over,
});

describe('cutover check', () => {
  test('passes when every project is managed, brought up from its file, and healthy', () => {
    const f = check([c('media-web-1', 'media', '/opt/stacks/media/compose.yaml'), c('media-db-1', 'media', '/opt/stacks/media/compose.yaml', 'Up 2 hours')], node());
    expect(f.filter((x) => !x.ok)).toEqual([]);
  });

  test('fails on another writer, an unmanaged project, drift, a second manager and unhealthy containers', () => {
    const f = check(
      [
        c('media-web-1', 'media', '/app/data/stacks/media/docker-compose.yml'),
        c('media-db-1', 'media', '/app/data/stacks/media/docker-compose.yml', 'Up 1 minute (unhealthy)'),
        c('loose', 'loose', '/srv/loose/compose.yaml'),
        c('bare', undefined, ''),
      ],
      node({
        stacks: [{ name: 'media', status: 'partial', path: '/opt/stacks/media', file: 'compose.yaml', drift: true, head: { short: 'abc1234' }, also_managed_by: [{ manager: 'Dockhand' }] }],
        detected: [{ name: 'loose', reason: null }],
      }),
    );
    const fails = f.filter((x) => !x.ok).map((x) => x.what);
    expect(fails).toEqual([
      'bare isn’t in a compose project, so no stack manages it'.replace('’', "'"),
      "loose runs but Cha Dash doesn't manage it",
      "media was last brought up from /app/data/stacks/media/docker-compose.yml, not Cha Dash's /opt/stacks/media/compose.yaml: another tool is its writer",
      "media's file was edited outside Cha Dash since its last commit",
      'Dockhand also lists a stack named media',
      'media-db-1 is Up 1 minute (unhealthy)',
    ]);
  });

  test('fails on a stopped stack, an offline node and a missing node', () => {
    expect(check([], node({ online: false })).map((x) => x.what)).toEqual([
      "the node pvedocker isn't connected to the hub",
      'media is managed but has no containers (status running)',
    ]);
    expect(check([c('media-web-1', 'media', '/opt/stacks/media/compose.yaml', 'Exited (0)', 'exited')], node())[1]).toEqual({ ok: false, what: 'media-web-1 is exited' });
    expect(check([], undefined)).toEqual([{ ok: false, what: 'the hub has no such node' }]);
  });

  test('reads a tcp:// endpoint as http', () => {
    expect(dockerBase('tcp://10.0.11.20:2375/')).toBe('http://10.0.11.20:2375');
    expect(dockerBase('https://docker.lab.example')).toBe('https://docker.lab.example');
  });
});
