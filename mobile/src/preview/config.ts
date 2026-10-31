/** Only a local debug build may replace real computers with screenshot data. */
export const storePreview = __DEV__ && process.env.EXPO_PUBLIC_PARTYLINE_SCREENSHOTS === '1';
