import { Navbar } from "@/components/Navbar";

export default function RootGroupLayout({ children }: LayoutProps<"/">) {
  return (
    <div className="w-full h-full flex flex-col items-center">
      <Navbar />
      <div className="w-full max-w-6xl">{children}</div>
    </div>
  );
}
