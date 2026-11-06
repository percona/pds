import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter } from "react-router";
import { Toaster } from "sonner";
import { FeatureFlagProvider } from "@/features/flags";
import { ThemeProvider } from "@/lib/theme";
import { RootRoutes } from "@/routes/root";
import { routerBasename } from "@/lib/base-path";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      refetchOnWindowFocus: false,
      retry: 1,
    },
  },
});

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <FeatureFlagProvider>
          <BrowserRouter basename={routerBasename}>
            <RootRoutes />
          </BrowserRouter>
          <Toaster position="bottom-right" duration={4000} />
        </FeatureFlagProvider>
      </ThemeProvider>
    </QueryClientProvider>
  );
}
