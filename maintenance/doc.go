// Package maintenance gates requests while the application is down for
// maintenance. Middleware answers 503 while the marker file the down
// command writes exists, serves the health probes and configured exclude
// paths normally, and lets a holder of the bypass secret through with a
// signed cookie. Config carries the marker root, the extra exclude paths
// and the app key the bypass MAC is salted with; velocity.ConfigFromEnv
// fills it from VELOCITY_MAINTENANCE_ROOT, VELOCITY_MAINTENANCE_EXCLUDE_PATHS
// and APP_KEY.
package maintenance
