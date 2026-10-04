/** Composables the widgets share: the subject's state, and the viewer's role. */
import { computed, type ComputedRef } from 'vue';
import { useLive } from '../stores/live';
import { useSession } from '../stores/session';
import { subjectOf, type Subject } from './subject';

/** The state of one resource (by id), live. */
export function useSubject(id: () => string | undefined): ComputedRef<Subject> {
  const live = useLive();
  return computed(() => subjectOf(live.graph.value, live.problems.value, live.repairs.value, id()));
}

/** The signed-in viewer's role, or undefined outside a session (previews). */
export function useRole(): () => string | undefined {
  try {
    const s = useSession();
    return () => s.state.user?.role;
  } catch {
    return () => undefined;
  }
}
