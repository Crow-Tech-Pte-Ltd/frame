// Wording for user reports (docs/ADMIN.md "Reports").
import type { ReportReason, ReportResolution, ReportTargetType } from "@/lib/types";

export const REASON_LABEL: Record<ReportReason, string> = {
  spam: "Spam",
  harassment: "Harassment or bullying",
  nudity: "Nudity or sexual content",
  violence: "Violence or threats",
  self_harm: "Self-harm",
  other: "Something else",
};

export const TARGET_LABEL: Record<ReportTargetType, string> = {
  moment: "Moment",
  message: "Message",
  user: "Profile",
};

export const RESOLUTION_LABEL: Record<ReportResolution, string> = {
  deleted_content: "Content deleted",
  suspended_user: "User suspended",
  dismissed: "Dismissed",
};

/** Tell the nav badge that the open-report count may have changed. */
export function reportsChanged() {
  window.dispatchEvent(new Event("frame:reports-changed"));
}
