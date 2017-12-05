import CommitIcon from '@sourcegraph/icons/lib/Commit'
import formatDistance from 'date-fns/formatDistance'
import * as H from 'history'
import * as React from 'react'
import { RepoBreadcrumb } from '../components/Breadcrumb'
import { DecoratedTextLines } from '../components/DecoratedTextLines'
import { GitRefTag } from '../repo/GitRefTag'
import { AbsoluteRepoFilePosition, RepoSpec } from '../repo/index'
import { UserAvatar } from '../settings/user/UserAvatar'
import { eventLogger } from '../tracking/eventLogger'
import { parseCommitDateString } from '../util/time'
import { toPrettyBlobURL } from '../util/url'
import { ResultContainer } from './ResultContainer'

interface Props {
    location: H.Location

    /**
     * The commit search result.
     */
    result: GQL.ICommitSearchResult

    /**
     * Called when the search result is selected.
     */
    onSelect: () => void

    /**
     * Whether this diff should be rendered as expanded.
     */
    expanded: boolean
}

export const CommitSearchResult: React.StatelessComponent<Props> = (props: Props) => {
    const telemetryData: { [key: string]: any } = {
        preview_type: props.result.diffPreview ? 'diff' : 'message',
    }
    const logClickOnPerson = () =>
        eventLogger.log('CommitSearchResultClicked', { commit_search_result: { ...telemetryData, target: 'person' } })
    const logClickOnMessage = () =>
        eventLogger.log('CommitSearchResultClicked', {
            commit_search_result: { ...telemetryData, target: 'message' },
        })
    const logClickOnTag = () =>
        eventLogger.log('CommitSearchResultClicked', {
            commit_search_result: { ...telemetryData, target: 'tag' },
        })
    const logClickOnCommitID = () =>
        eventLogger.log('CommitSearchResultClicked', {
            commit_search_result: { ...telemetryData, target: 'commit-id' },
        })
    const logClickOnTimestamp = () =>
        eventLogger.log('CommitSearchResultClicked', {
            commit_search_result: { ...telemetryData, target: 'timestamp' },
        })
    const logClickOnText = () =>
        eventLogger.log('CommitSearchResultClicked', {
            commit_search_result: { ...telemetryData, target: 'text' },
        })

    const commitURL = `https://${props.result.commit.repository.uri}/commit/${props.result.commit.oid}`
    const title: React.ReactChild = (
        <div className="commit-search-result__title">
            <RepoBreadcrumb repoPath={props.result.commit.repository.uri} rev={props.result.commit.oid} filePath={''} />
            <a
                href={commitURL}
                className="commit-search-result__title-person"
                onClick={stopPropagationToCollapseOrExpand}
                onMouseDown={logClickOnPerson}
            >
                <UserAvatar user={props.result.commit.author.person!} size={32} />{' '}
                {props.result.commit.author.person!.displayName}
            </a>
            <a
                href={commitURL}
                className="commit-search-result__title-message"
                onClick={stopPropagationToCollapseOrExpand}
                onMouseDown={logClickOnMessage}
            >
                {commitMessageSubject(props.result.commit.message) || '(empty commit message)'}
            </a>
            <span className="commit-search-result__title-signature">
                {uniqueRefs([...props.result.refs, ...props.result.sourceRefs]).map((ref, i) => (
                    <GitRefTag key={i} gitRef={ref} onMouseDown={logClickOnTag} />
                ))}
                <code>
                    <a href={commitURL} onClick={stopPropagationToCollapseOrExpand} onMouseDown={logClickOnCommitID}>
                        {props.result.commit.abbreviatedOID}
                    </a>
                </code>{' '}
                <a href={commitURL} onClick={stopPropagationToCollapseOrExpand} onMouseDown={logClickOnTimestamp}>
                    {formatDistance(parseCommitDateString(props.result.commit.author.date), new Date(), {
                        addSuffix: true,
                    })}
                </a>
            </span>
        </div>
    )

    const expandedChildren: JSX.Element[] = []

    if (props.result.messagePreview) {
        expandedChildren.push(
            <DecoratedTextLines
                key="messagePreview"
                className="file-match__item commit-search-result__item commit-search-result__item-message-preview"
                value={props.result.messagePreview.value.trim().split('\n')}
                highlights={props.result.messagePreview.highlights}
                lineClasses={[{ line: 1, className: 'strong' }]}
                onMouseDown={logClickOnText}
            />
        )
    }

    if (props.result.diffPreview) {
        const commonCtx: RepoSpec = {
            repoPath: props.result.commit.repository.uri,
        }
        // lhsCtx and rhsCtx need the cast because their values at const init time lack
        // the filePath field, which is assigned as we iterate over the lines below.
        const lhsCtx = {
            ...commonCtx,
            commitID: props.result.commit.oid + '~',
            rev: props.result.commit.oid + '~',
        } as AbsoluteRepoFilePosition
        const rhsCtx = {
            ...commonCtx,
            commitID: props.result.commit.oid,
            rev: props.result.commit.oid,
        } as AbsoluteRepoFilePosition

        // Omit "index ", "--- file", and "+++ file" lines.
        const lines = props.result.diffPreview.value.split('\n')
        const lineClasses: { line: number; className: string; url?: string }[] = []
        let ignoreUntilAtAt = false
        for (const [i, line] of lines.entries()) {
            if (ignoreUntilAtAt && !line.startsWith('@@')) {
                lineClasses.push({ line: i + 1, className: 'hidden' })
                continue
            } else {
                ignoreUntilAtAt = false
            }
            if (line.startsWith('diff ')) {
                ignoreUntilAtAt = true

                // Simplify from "diff --git origname newname".
                const [origName, newName] = line.replace(/^diff --git /, '').split(' ')
                let simplerLine: string
                if (origName === newName) {
                    simplerLine = origName
                } else {
                    simplerLine = `${origName} -> ${newName} (renamed)`
                }
                lines[i] = simplerLine

                lhsCtx.filePath = newName
                rhsCtx.filePath = newName
                lineClasses.push({ line: i + 1, className: 'file-header', url: toPrettyBlobURL(rhsCtx) })
            } else if (line.startsWith('@@')) {
                // TODO(sqs): a bit hacky getting the position
                try {
                    const m = line.match(/^@@ -(\d+),.*\+(\d+)/)
                    if (m) {
                        if (m[1]) {
                            const lhsLine = parseInt(m[1], 10)
                            lhsCtx.position = { line: lhsLine - 1, character: 0 }
                        }
                        if (m[2]) {
                            const rhsLine = parseInt(m[2], 10)
                            rhsCtx.position = { line: rhsLine - 1, character: 0 }
                        }
                    }
                } catch (err) {
                    // TODO(sqs)
                    console.error(err)
                }

                lineClasses.push({ line: i + 1, className: 'hunk-header', url: toPrettyBlobURL(rhsCtx) })
            } else {
                if (rhsCtx.position && rhsCtx.position.line) {
                    if (!line.startsWith('+')) {
                        lhsCtx.position.line++
                    }
                    if (!line.startsWith('-')) {
                        rhsCtx.position.line++
                    }
                }

                if (line.startsWith('+')) {
                    lineClasses.push({ line: i + 1, className: 'added', url: toPrettyBlobURL(rhsCtx) })
                } else if (line.startsWith('-')) {
                    lineClasses.push({
                        line: i + 1,
                        className: 'deleted',
                        url: toPrettyBlobURL(lhsCtx),
                    })
                }
            }
        }

        expandedChildren.push(
            <DecoratedTextLines
                key="diffPreview"
                className="file-match__item commit-search-result__item commit-search-result__item-diff-preview"
                value={lines}
                highlights={props.result.diffPreview.highlights}
                lineClasses={lineClasses}
                onMouseDown={logClickOnText}
            />
        )
    }

    return (
        <ResultContainer
            collapsible={true}
            defaultExpanded={true}
            icon={CommitIcon}
            title={title}
            expandedChildren={expandedChildren}
        />
    )
}

function commitMessageSubject(message: string): string {
    const eol = message.indexOf('\n')
    return eol === -1 ? message : message.slice(0, eol)
}

function stopPropagationToCollapseOrExpand(e: React.MouseEvent<HTMLElement>): void {
    e.stopPropagation()
}

function uniqueRefs(refs: GQL.IGitRef[]): GQL.IGitRef[] {
    const seenName = new Set<string>()
    const uniq: GQL.IGitRef[] = []
    for (const ref of refs) {
        if (!seenName.has(ref.name)) {
            uniq.push(ref)
            seenName.add(ref.name)
        }
    }
    return uniq
}
