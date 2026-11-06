import { visualSheets } from './sheet';

// Shard 1 of 4, so the browser runs the sheets in parallel pages.
visualSheets(1, 4);
