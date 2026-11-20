import { SiteFooter } from "@/components/site-footer";
import { SiteHeader } from "@/components/site-header";
import { Faq } from "@/sections/faq";
import { Hero } from "@/sections/hero";
import { Operations } from "@/sections/operations";
import { Proof } from "@/sections/proof";
import { Pricing } from "@/sections/pricing";

export function App() {
  return (
    <div className="min-h-screen bg-background text-foreground">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:fixed focus:top-3 focus:left-3 focus:z-50 focus:rounded-md focus:bg-card focus:px-3 focus:py-2 focus:text-sm"
      >
        Skip to content
      </a>
      <SiteHeader />
      <main id="main">
        <Hero />
        <Proof />
        <Operations />
        <Pricing />
        <Faq />
      </main>
      <SiteFooter />
    </div>
  );
}
