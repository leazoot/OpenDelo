import { Link } from 'react-router'

import { useCopy } from '../i18n/copy'

import { Placeholder } from './Placeholder'
import styles from './Placeholder.module.css'

/**
 * 认不出的路径。
 *
 * 说明发生了什么并给出下一步，而不是白屏。
 */
export function NotFoundPage() {
  const copy = useCopy()

  return (
    <Placeholder title={copy.pageNotFound} blurb={copy.pageNotFoundBlurb}>
      <Link className={styles.link} to="/gate">
        {copy.backToGate}
      </Link>
    </Placeholder>
  )
}
