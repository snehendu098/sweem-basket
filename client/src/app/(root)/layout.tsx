import { Navbar } from "@/components/Navbar";

export default function RootGroupLayout({ children }: LayoutProps<"/">) {
  // The navbar lays out its own children full width, outside the constrained
  // wrapper, so the wordmark stays at the left margin. Pages render inside the
  // max-w-6xl container and never re-declare a width of their own.
  return (
    <div className="w-full h-full flex flex-col items-center">
      <Navbar />
      <div className="w-full max-w-6xl">{children}</div>
    </div>
  );
}
