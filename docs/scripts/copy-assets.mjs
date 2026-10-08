import { copyFileSync, mkdirSync } from 'node:fs';

mkdirSync(new URL('../public/', import.meta.url), { recursive: true });
copyFileSync(new URL('../../demo/demo.gif', import.meta.url), new URL('../public/demo.gif', import.meta.url));
