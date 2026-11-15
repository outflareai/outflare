import {
  CircleCheckIcon,
  InfoIcon,
  Loader2Icon,
  OctagonXIcon,
  TriangleAlertIcon,
} from "lucide-react"
import { useSyncExternalStore } from "react"
import { Toaster as Sonner, type ToasterProps } from "sonner"

// Toasts sit at the top right, under the header, so they never cover a chat
// box, a send button or the phone keyboard. Each has a close button, can be
// swiped away, and at most three show at once.

// Follows the app's theme, which lives in <html data-theme>.
function subscribe(onChange: () => void) {
  const o = new MutationObserver(onChange)
  o.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] })
  return () => o.disconnect()
}

const Toaster = ({ ...props }: ToasterProps) => {
  const theme = useSyncExternalStore(subscribe, () => (document.documentElement.dataset.theme === "light" ? "light" : "dark"))

  return (
    <Sonner
      theme={theme as ToasterProps["theme"]}
      className="toaster group"
      position="top-right"
      offset={{ top: 56, right: 16 }}
      mobileOffset={{ top: 56, left: 12, right: 12 }}
      visibleToasts={3}
      closeButton
      swipeDirections={["right", "left", "top"]}
      duration={4000}
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
      {...props}
    />
  )
}

export { Toaster }
