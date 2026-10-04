// The engine alone, no Vue: every operation a page needs, so nothing is tree-shaken away.
import { commit, createBoard, createHistory, deriveLayout, deserialize, move, resize, serialize, smartInsert, snapSize, stepSize, nudge, undo, type BoardInput } from '@chadash/layout';
import { DOGFOOD, PIHOLE } from '../../scenarios/index.ts';

let board = createBoard(DOGFOOD as unknown as BoardInput);
let history = createHistory(board);
board = move(board, 'fleet', { x: 4, y: 0 });
board = resize(board, 'plex', snapSize(['small', 'wide', 'large'], 4, 4, 8) ?? 'wide');
board = nudge(board, 'backups', 'left');
board = smartInsert(board, { ...PIHOLE, supportedSizes: [...PIHOLE.supportedSizes] });
history = undo(commit(history, board));
const out = [deriveLayout(board, 'phone'), stepSize(['small', 'wide'], 'small', 'wider', 8), deserialize(serialize(history.present))];
document.body.textContent = JSON.stringify(out);
