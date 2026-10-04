/**
 * Where the owner is in "Connect your home", the first-run wizard. The hub
 * keeps it (one settings row), so the wizard shows once per hub, not once
 * per browser:
 *
 *   GET /api/onboarding → {status: new|started|skipped|done, step, show, sidecar, updated_at}
 *   PUT /api/onboarding   {status: started|skipped|done, step?}
 *
 * `show` is the hub's call: a new hub with nothing connected, or a wizard
 * that was started and not finished. Skip and Finish end it for good; it can
 * still be opened again by hand (Settings → Integrations → Setup guide).
 *
 * Tiny and shared: the signed-in shell loads it to decide, the wizard and
 * Settings use it to open and close.
 */
import { reactive } from 'vue';
import { api } from '../lib/api';

export type OnboardingStatus = 'new' | 'started' | 'skipped' | 'done';
export type WizardStep = 'start' | 'import' | 'discover' | 'add' | 'done';

const STEPS: readonly WizardStep[] = ['start', 'import', 'discover', 'add', 'done'];

interface Remote {
  status: OnboardingStatus;
  step: string;
  show: boolean;
  sidecar: string;
}

export const onboarding = reactive({
  /** The wizard is on screen. */
  open: false,
  status: 'new' as OnboardingStatus,
  step: 'start' as WizardStep,
  /** The compose sidecar's socket proxy, as the hub names it. */
  sidecar: 'tcp://socket-proxy:2375',
});

const asStep = (s: string | undefined): WizardStep => (STEPS.includes(s as WizardStep) ? (s as WizardStep) : 'start');

/** Ask the hub, and open the wizard when it says to. Quiet on failure: the page matters more. */
export async function loadOnboarding(): Promise<void> {
  try {
    const r = await api<Remote>('/api/onboarding');
    onboarding.status = r.status;
    onboarding.sidecar = r.sidecar || onboarding.sidecar;
    if (r.show && !onboarding.open) {
      onboarding.step = asStep(r.step);
      onboarding.open = true;
      if (r.status === 'new') void save('started', onboarding.step);
    }
  } catch {
    /* an older hub without the route, or offline: no wizard */
  }
}

async function save(status: Exclude<OnboardingStatus, 'new'>, step?: WizardStep): Promise<void> {
  onboarding.status = status;
  try {
    await api('/api/onboarding', { method: 'PUT', body: step ? { status, step } : { status } });
  } catch {
    /* kept locally; the hub asks again next time */
  }
}

/** Move to a step, remembered so a reload resumes there. */
export function goTo(step: WizardStep): void {
  onboarding.step = step;
  void save('started', step);
}

/** Open the wizard by hand, where it was left (or at the start once finished). */
export function openWizard(step?: WizardStep): void {
  const resume = onboarding.status === 'started' ? onboarding.step : 'start';
  onboarding.step = step ?? resume;
  onboarding.open = true;
  void save('started', onboarding.step);
}

/**
 * Close it: `later` keeps it started (it comes back on the next visit),
 * `skipped` and `done` end it.
 */
export async function closeWizard(how: 'later' | 'skipped' | 'done'): Promise<void> {
  onboarding.open = false;
  if (how === 'later') await save('started', onboarding.step);
  else await save(how);
}

/** For tests. */
export function resetOnboarding(): void {
  Object.assign(onboarding, { open: false, status: 'new', step: 'start', sidecar: 'tcp://socket-proxy:2375' });
}
