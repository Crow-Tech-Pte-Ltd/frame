import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

// Copied from crow-tech-landing lib/utils.ts.
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
