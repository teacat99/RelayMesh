import { cva, type VariantProps } from 'class-variance-authority'

export const alertVariants = cva(
  'relative w-full rounded-md border px-4 py-3 text-sm [&>svg~*]:pl-7 [&>svg+div]:translate-y-[-3px] [&>svg]:absolute [&>svg]:left-4 [&>svg]:top-3.5',
  {
    variants: {
      variant: {
        default:
          'bg-card text-card-foreground [&>svg]:text-foreground',
        info:
          'bg-brand-50 text-brand-700 border-brand-100 [&>svg]:text-brand-600 dark:bg-brand-900/30 dark:text-brand-200 dark:border-brand-800',
        warning:
          'bg-amber-50 text-amber-800 border-amber-200 [&>svg]:text-amber-600 dark:bg-amber-900/20 dark:text-amber-200 dark:border-amber-800',
        destructive:
          'border-destructive/30 bg-destructive/5 text-destructive [&>svg]:text-destructive',
        success:
          'bg-emerald-50 text-emerald-800 border-emerald-200 [&>svg]:text-emerald-600 dark:bg-emerald-900/20 dark:text-emerald-200 dark:border-emerald-800'
      }
    },
    defaultVariants: {
      variant: 'default'
    }
  }
)

export type AlertVariants = VariantProps<typeof alertVariants>
