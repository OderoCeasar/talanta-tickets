// Baked in at build time from the profile's `env` in eas.json. In development, set it in
// mobile/.env.local to your machine's LAN address (a phone cannot reach "localhost").
export const API_URL = process.env.EXPO_PUBLIC_API_URL ?? "http://localhost:8080";
