// tmux pane ids look like "%21", and a "%" in a route path is read as URL encoding: "%21" arrived
// as "!", and the session screen waited for a pane that does not exist. Routes carry the number
// alone; the screen puts the "%" back.

/** "%21" → "21", for a session route. */
export const paneToRoute = (pane: string) => pane.replace(/^%/, '');

/** A session route's pane back to the tmux id: "21" → "%21"; anything else is kept as it is. */
export const paneFromRoute = (pane: string) => (/^\d+$/.test(pane) ? `%${pane}` : pane);
