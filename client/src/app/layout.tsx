import type { Metadata } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import "./globals.css";
import { SessionProvider } from "@/lib/session";
import { Nav } from "@/components/ui";

const geistSans = Geist({ variable: "--font-geist-sans", subsets: ["latin"] });
const geistMono = Geist_Mono({ variable: "--font-geist-mono", subsets: ["latin"] });

export const metadata: Metadata = {
  title: "Basket — non-custodial yield baskets",
  description:
    "Subscribe to a yield basket. Funds never leave your own wallet; a bounded delegated signer follows the weights for you.",
};

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html
      lang="en"
      className={`${geistSans.variable} ${geistMono.variable} h-full antialiased`}
    >
      <body className="min-h-full flex flex-col">
        <SessionProvider>
          <Nav />
          <main className="mx-auto w-full max-w-6xl flex-1 px-5 py-8">
            {children}
          </main>
          <footer className="border-t border-zinc-800 px-5 py-4 text-center text-xs text-zinc-600">
            Funds stay in your own Privy embedded wallet. Delegation is
            revocable at any time.
          </footer>
        </SessionProvider>
      </body>
    </html>
  );
}
