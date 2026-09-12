import type { Metadata } from "next";
import { Poppins } from "next/font/google";
import "./globals.css";
import { ChainProvider } from "@/lib/chain";
import { SessionProvider } from "@/lib/session";
import { Toaster } from "@/components/Toaster";

const poppins = Poppins({
  variable: "--font-geist-sans",
  subsets: ["latin"],
  weight: ["400", "500", "600", "700"],
});

export const metadata: Metadata = {
  title: "sweem",
  description: "Non-custodial yield baskets on Base.",
};

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html
      lang="en"
      className={`dark ${poppins.variable} h-full`}
    >
      <body className={`${poppins.className} min-h-full bg-background text-foreground antialiased`}>
        <ChainProvider>
          <SessionProvider>{children}</SessionProvider>
        </ChainProvider>
        <Toaster />
      </body>
    </html>
  );
}
