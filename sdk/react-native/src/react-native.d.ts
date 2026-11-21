// The few parts of React Native this library uses, so it type-checks
// without React Native installed. The app's own React Native is what runs.
declare module "react-native" {
  export const Platform: {
    OS: string;
    Version: string | number;
    isPad?: boolean;
    constants?: { Model?: string; Manufacturer?: string; Brand?: string };
  };
  export const AppState: {
    addEventListener(type: "change", listener: (state: string) => void): { remove(): void };
  };
}
