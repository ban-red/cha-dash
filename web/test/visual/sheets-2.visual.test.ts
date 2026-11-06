import { visualSheets } from './sheet';

// Shard 2 of 4, so the browser runs the sheets in parallel pages.
visualSheets(2, 4);
