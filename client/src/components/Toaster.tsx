"use client";

import {
  CircleCheckIcon,
  InfoIcon,
  Loader2Icon,
  OctagonXIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { Toaster as Sonner, type ToasterProps } from "sonner";

/**
 * One theme, so no next-themes: the app is dark everywhere and the toast reads
 * from the same tokens as every other surface. Only the icon carries the tone
 * — a fully tinted toast next to the lime primary is noise, and a warning must
 * not look like an error.
 */
const Toaster = (props: ToasterProps) => (
  <Sonner
    theme="dark"
    position="bottom-right"
    className="toaster group"
    icons={{
      success: <CircleCheckIcon className="size-4" />,
      info: <InfoIcon className="size-4" />,
      warning: <TriangleAlertIcon className="size-4" />,
      error: <OctagonXIcon className="size-4" />,
      loading: <Loader2Icon className="size-4 animate-spin" />,
    }}
    style={
      {
        "--normal-bg": "var(--popover)",
        "--normal-text": "var(--popover-foreground)",
        "--normal-border": "var(--border)",
        "--border-radius": "var(--radius)",
      } as React.CSSProperties
    }
    toastOptions={{
      classNames: {
        toast: "cn-toast",
        success: "[&_[data-icon]]:text-positive",
        warning: "[&_[data-icon]]:text-warning",
        error: "[&_[data-icon]]:text-destructive",
        description: "!text-muted-foreground",
        actionButton: "!bg-secondary !text-foreground",
      },
    }}
    {...props}
  />
);

export { Toaster };
